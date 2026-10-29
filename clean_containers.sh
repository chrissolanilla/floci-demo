#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

eval "$(floci env --host localhost)"

set -a
source .env
set +a

export AWS_PAGER=""

docker compose up -d floci

#find ec2 and kill it
instance=$(aws \
    --endpoint-url "$AWS_ENDPOINT_URL" \
    --region "$AWS_REGION" \
    ec2 describe-instances \
    --filters \
        "Name=tag:Name,Values=floci-demo-app" \
        "Name=instance-state-name,Values=pending,running,stopped" \
    --query "Reservations[].Instances[].InstanceId" \
    --output text)

if [[ -n "$instance" && "$instance" != "None" ]]; then
    echo "Terminating EC2: $instance"

    aws \
        --endpoint-url "$AWS_ENDPOINT_URL" \
        --region "$AWS_REGION" \
        ec2 terminate-instances \
        --instance-ids "$instance"

    aws \
        --endpoint-url "$AWS_ENDPOINT_URL" \
        --region "$AWS_REGION" \
        ec2 wait instance-terminated \
        --instance-ids "$instance"

    echo "EC2 terminated."
fi

docker compose stop app 2>/dev/null || true

docker compose stop floci

echo "Floci stopped."
