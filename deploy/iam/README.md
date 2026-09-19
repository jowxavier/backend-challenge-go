# Broker permissions

LocalStack uses fake test/test credentials. This local setup does not enforce
production IAM authorization; it must not be exposed as a trusted public broker.

For AWS, replace the example account and region in these identity policies and
attach them to separate runtime and trusted internal producer roles. Set
SQS_ENDPOINT to an empty string and use the SDK role credential chain. Do not
export local test credentials in deployment.

runtime-policy.json contains only the permissions the combined consumer,
publisher, startup/readiness checks and DLQ depth sampler use. The runtime cannot
send commands, read output events, consume DLQ messages or provision queues.
producer-policy.json permits trusted internal command producers to send only to
the inbound queue. External providers must use authenticated HTTP: a producer
role is trusted to choose providerId and must never be issued to providers.

Keep queues private within the account, without public/cross-account allow
policies. Review other attached identity policies to prevent broader grants.
Use separate administrator credentials for queue creation, redrive configuration
and policy management. Provisioner privileges are not runtime privileges.
No cloud deployment system or IAM enforcement claim for the emulator is included.
