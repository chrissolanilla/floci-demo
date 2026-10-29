set -euo pipefail

cd "$(dirname "$0")"

for tool in docker floci aws go curl npm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "Missing required tool: $tool" >&2
        exit 1
    fi
done

eval "$(floci env --host localhost)"

set -a
source .env
set +a

export AWS_PAGER=""

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

go run scripts/provision.go

DB_HOST=$(aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    rds describe-db-instances \
    --db-instance-identifier "$DB_INSTANCE" \
    --query "DBInstances[0].Endpoint.Address" \
    --output text)

if [[ -z "$DB_HOST" || "$DB_HOST" == "None" ]]; then
    echo "Failed to find RDS endpoint" >&2
    exit 1
fi

echo "RDS endpoint: $DB_HOST"

#find ec2
instance=$(aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    ec2 describe-instances \
    --filters \
        "Name=tag:Name,Values=floci-demo-app" \
        "Name=instance-state-name,Values=running" \
    --query "Reservations[].Instances[].InstanceId" \
    --output text)

if [[ -z "$instance" || "$instance" == *$'\t'* ]]; then
    echo "Expected exactly one running EC2 instance, got: $instance" >&2
    exit 1
fi

echo "EC2 instance: $instance"

#find floci containers
container=$(docker ps -q \
    --filter "label=io.floci.service=ec2" \
    --filter "label=io.floci.resource-id=$instance")

if [[ -z "$container" ]]; then
    echo "EC2 container not found for $instance" >&2
    echo "Check that Floci has access to the Docker socket." >&2
    exit 1
fi

#we need to connect to vpc network
vpc_network=$(docker inspect "$container" \
    --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' |
    grep 'floci-aws-.*-vpc-' |
    head -n 1)

if [[ -z "$vpc_network" ]]; then
    echo "EC2 VPC network not found" >&2
    exit 1
fi

floci_ip=$(docker inspect floci-demo-floci-1 \
    --format "{{with index .NetworkSettings.Networks \"$vpc_network\"}}{{.IPAddress}}{{end}}")

if [[ -z "$floci_ip" ]]; then
    echo "Floci is not connected to the EC2 VPC network" >&2
    exit 1
fi

ec2_aws_endpoint="http://$floci_ip:4566"

echo "EC2 AWS endpoint: $ec2_aws_endpoint"

#incase we get weird errors
docker exec "$container" \
    wget -qO /dev/null "$ec2_aws_endpoint/_localstack/health" || {
        echo "EC2 cannot reach Floci" >&2
        exit 1
    }

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
        wget -qO- http://127.0.0.1:8080/api/health \
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
