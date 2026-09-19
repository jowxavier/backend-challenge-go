#!/bin/sh
set -eu
region="${AWS_DEFAULT_REGION:-us-east-1}"
max_count="${SQS_MAX_RECEIVE_COUNT:-5}"
visibility="${SQS_VISIBILITY_SECONDS:-60}"
case "$max_count" in ''|*[!0-9]*) exit 1;; esac
case "$visibility" in ''|*[!0-9]*) exit 1;; esac
[ "$max_count" -gt 0 ]
dlq_url=$(awslocal sqs create-queue --region "$region" --queue-name wager-transactions-dlq.fifo --attributes FifoQueue=true --query QueueUrl --output text)
dlq_arn=$(awslocal sqs get-queue-attributes --region "$region" --queue-url "$dlq_url" --attribute-names QueueArn --query Attributes.QueueArn --output text)
input_url=$(awslocal sqs create-queue --region "$region" --queue-name wager-transactions.fifo --attributes FifoQueue=true --query QueueUrl --output text)
attributes=$(python3 -c 'import json,sys; print(json.dumps({"RedrivePolicy":json.dumps({"deadLetterTargetArn":sys.argv[1],"maxReceiveCount":int(sys.argv[2])}),"VisibilityTimeout":sys.argv[3],"ReceiveMessageWaitTimeSeconds":"20"}))' "$dlq_arn" "$max_count" "$visibility")
awslocal sqs set-queue-attributes --region "$region" --queue-url "$input_url" --attributes "$attributes"
awslocal sqs create-queue --region "$region" --queue-name wager-events >/dev/null
