#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

for tool in docker floci aws go curl npm cdk; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "Missing required tool: $tool" >&2
        exit 1
    fi
done

#this allows floci to hijack aws cli so they dont actually go to aws and instead go to local floci containers
eval "$(floci env --host localhost)"

set -a
source .env
set +a

export AWS_REGION="us-east-1"
export AWS_DEFAULT_REGION="$AWS_REGION"

#alwayss dummy creds
export AWS_ACCESS_KEY_ID="test"
export AWS_SECRET_ACCESS_KEY="test"

export AWS_EC2_METADATA_DISABLED=true
export AWS_PAGER=""

#never allow deployment to real AWS
case "${AWS_ENDPOINT_URL:-}" in
    http://localhost:4566|http://127.0.0.1:4566)
        ;;
    *)
        echo "ERROR: Refusing to deploy to non-local AWS endpoint." >&2
        echo "AWS_ENDPOINT_URL=${AWS_ENDPOINT_URL:-unset}" >&2
        exit 1
        ;;
esac

required_vars=(
    AWS_REGION
    AWS_ENDPOINT_URL
    S3_BUCKET
    DB_INSTANCE
    DB_PORT
    DB_USER
    DB_PASSWORD
    DB_NAME
)

for var in "${required_vars[@]}"; do
    if [[ -z "${!var:-}" ]]; then
        echo "ERROR: Missing required variable: $var" >&2
        exit 1
    fi
done

echo "AWS region: $AWS_REGION"
echo "AWS endpoint: $AWS_ENDPOINT_URL"
echo "Environment configured successfully."

docker compose up -d floci

for attempt in {1..60}; do
    if curl -fsS \
        "$AWS_ENDPOINT_URL/_localstack/health" \
        >/dev/null 2>&1; then
        break
    fi

    if (( attempt == 60 )); then
        echo "Floci did not start" >&2
        exit 1
    fi

    sleep 1
done

echo "Synthesizing CDK infrastructure..."
#parenthiss creates a subshell so its important
(
    cd cdk
    cdk synth FlociDemoStack --quiet
)

echo "Deploying CloudFormation stack to Floci..."
aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    cloudformation deploy \
    --template-file cdk/cdk.out/FlociDemoStack.template.json \
    --stack-name FlociDemoStack \
    --capabilities CAPABILITY_IAM \
    --no-fail-on-empty-changeset

echo "CDK infrastructure deployed!"

DB_HOST=$(aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    cloudformation describe-stacks \
    --stack-name FlociDemoStack \
    --query "Stacks[0].Outputs[?OutputKey=='DatabaseEndpoint'].OutputValue | [0]" \
    --output text)


if [[ -z "$DB_HOST" || "$DB_HOST" == "None" ]]; then
    echo "Failed to find RDS endpoint" >&2
    exit 1
fi

echo "RDS endpoint: $DB_HOST"

echo "Waiting for PostgreSQL..."

#rds wait instace avail?
aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    rds wait db-instance-available \
    --db-instance-identifier "$DB_INSTANCE"

#get the instance and verifiy it(floci can only accept some instances, not real actual amis?)
instance=$(aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    cloudformation describe-stacks \
    --stack-name FlociDemoStack \
    --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue | [0]" \
    --output text)

if [[ -z "$instance" || "$instance" == "None" || "$instance" == *$'\t'* ]]; then
    echo "Invalid EC2 instance ID: $instance" >&2
    exit 1
fi

aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    ec2 wait instance-running \
    --instance-ids "$instance"

#find containers
echo "EC2 instance: $instance"

#find Floci EC2 container
container=$(docker ps -q \
    --filter "label=io.floci.service=ec2" \
    --filter "label=io.floci.resource-id=$instance")

if [[ -z "$container" ]]; then
    echo "EC2 container not found for $instance" >&2
    echo "Check that Floci has access to the Docker socket." >&2
    exit 1
fi

if [[ "$container" == *$'\n'* ]]; then
    echo "Multiple EC2 containers found for $instance" >&2
    exit 1
fi

echo "EC2 container: $container"

#we need to connect to vpc network
vpc_network=$(docker inspect "$container" \
    --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' |
    grep -m 1 'floci-aws-.*-vpc-' || true)

if [[ -z "$vpc_network" ]]; then
    echo "EC2 VPC network not found" >&2
    exit 1
fi

floci_container=$(docker compose ps -q floci)

if [[ -z "$floci_container" ]]; then
    echo "Floci container not found" >&2
    exit 1
fi

floci_ip=$(docker inspect "$floci_container" \
    --format "{{with index .NetworkSettings.Networks \"$vpc_network\"}}{{.IPAddress}}{{end}}")

if [[ -z "$floci_ip" ]]; then
    echo "Floci is not connected to the EC2 VPC network" >&2
    exit 1
fi

ec2_aws_endpoint="http://$floci_ip:4566"

echo "EC2 AWS endpoint: $ec2_aws_endpoint"

#incase we get weird errors, this image dosent have wget so
echo "Checking EC2 connectivity to Floci..."

if docker exec "$container" sh -c 'command -v curl >/dev/null 2>&1'; then
    docker exec "$container" \
        curl -fsS -o /dev/null "$ec2_aws_endpoint/_localstack/health"
else
    echo "curl is not installed in the EC2 container." >&2
    exit 1
fi

#build frontend
echo "Building frontend..."

(
    cd frontend
    npm ci
    npm run build
)

#build go
echo "Building backend..."

arch=$(docker exec "$container" uname -m)

case "$arch" in
    x86_64) goarch=amd64 ;;
    aarch64) goarch=arm64 ;;
    *)
        echo "Unsupported EC2 architecture: $arch" >&2
        exit 1
        ;;
esac

mkdir -p .deploy

(
    cd backend
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
        go build -o ../.deploy/server .
)

#copy to ec2
echo "Deploying application..."

docker exec "$container" mkdir -p /opt/app/dist

docker cp frontend/dist/. "$container:/opt/app/dist/"

#stop older deployment if pres
docker exec "$container" sh -c \
	'pkill -f "^/opt/app/server$" 2>/dev/null || true'

docker cp .deploy/server "$container:/opt/app/server"

docker exec "$container" chmod +x /opt/app/server

docker exec -d \
    -w /opt/app \
    -e "BACKEND_PORT=8080" \
    -e "DB_HOST=${DB_HOST:-}" \
    -e "DB_PORT=$DB_PORT" \
    -e "DB_USER=$DB_USER" \
    -e "DB_PASSWORD=$DB_PASSWORD" \
    -e "DB_NAME=$DB_NAME" \
    -e "S3_BUCKET=$S3_BUCKET" \
    -e "AWS_REGION=$AWS_REGION" \
	-e "FRONTEND_DIST=/opt/app/dist" \
	-e "AWS_ENDPOINT_URL=$ec2_aws_endpoint" \
	-e "LOG_PATH=/opt/app/app.log" \
	-e "AWS_ACCESS_KEY_ID=test" \
	-e "AWS_SECRET_ACCESS_KEY=test" \
    "$container" \
    /opt/app/server


echo "Checking backend..."

healthy=false

for attempt in {1..15}; do
    if docker exec "$container" \
        curl -fsS http://127.0.0.1:8080/api/health \
        >/dev/null 2>&1; then

        healthy=true
        break
    fi

    sleep 1
done

if [[ "$healthy" != true ]]; then
    echo "Backend failed to start." >&2

    docker exec "$container" \
        sh -c 'cat /opt/app/app.log 2>/dev/null || true'

    exit 1
fi

echo "Application running inside EC2: $instance"
echo "Check localhost:30000"
