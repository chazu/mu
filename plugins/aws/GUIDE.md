mu guide plugin aws — AWS resource observer

Observes AWS resource state via the AWS CLI. Returns structured JSON
records for drift detection with pudl.

USAGE IN mu.json

  {
    "target": "//infra/aws-inventory",
    "toolchain": "aws",
    "sources": [],
    "config": {
      "profile": "production",
      "region": "us-east-1",
      "resources": ["ec2", "vpc", "subnet"]
    }
  }

CONFIG FIELDS

  profile      AWS CLI profile name (required).
  region       AWS region (required).
  resources    List of resource types to observe (required).
               Valid types: "ec2", "vpc", "subnet".

EXAMPLES

  Observe all resource types:
    {"profile": "prod", "region": "us-east-1",
     "resources": ["ec2", "vpc", "subnet"]}

  EC2 instances only:
    {"profile": "dev", "region": "eu-west-1", "resources": ["ec2"]}

OBSERVATION OUTPUT

  mu observe --json //infra/aws-inventory

  Records include _schema, account, and region fields for pudl routing:

  EC2 instances (_schema: "aws.ec2.instance"):
    instance_id, instance_type, state, vpc_id, subnet_id, private_ip,
    public_ip, image_id, tags, security_groups, iam_profile

  VPCs (_schema: "aws.ec2.vpc"):
    vpc_id, cidr_block, state, is_default, tags, instance_tenancy

  Subnets (_schema: "aws.ec2.subnet"):
    subnet_id, vpc_id, cidr_block, availability_zone, state,
    map_public_ip_on_launch, available_ip_count, tags

SCHEMA OWNERSHIP

  This plugin declares and ships its wire-format schemas as mu/aws@v1.
  Discovery advertises the schema definition for each emitted resource type.
  The bundled pudl.cue file maps those resource types to PUDL's semantic
  schemas; the wire schema and semantic schema are deliberately separate.

PIPING TO PUDL

  mu observe --ndjson //infra/aws-inventory | pudl import --stdin

  Each record streams as one line with _schema for pudl routing.

PREREQUISITES

  AWS CLI v2 must be installed and configured with the named profile.
  Discovery is dependency-free; the plugin validates the CLI when observe
  runs.

CAPABILITIES

  discover, observe

CONTRACT FIXTURE

  Run the deterministic observer fixture without contacting AWS:

    MU_AWS_FIXTURE=1 \
      PATH="$PWD/plugins/aws/testdata/bin:$PATH" \
      mu plugin test plugins/aws
