mu guide plugin docker — Docker image builder

Builds Docker/OCI images from Dockerfiles.

USAGE IN mu.json

  {
    "target": "//images/myapp",
    "toolchain": "docker",
    "sources": ["Dockerfile", "src/**"],
    "config": {
      "tag": "myapp:latest",
      "context": "."
    }
  }

CONFIG FIELDS

  dockerfile    Path to Dockerfile (default: "Dockerfile").
  tag           Image tag (default: target short name).
  context       Build context directory (default: ".").
  target        Multi-stage build target name (optional).
  platform      Target platform (e.g. "linux/amd64").
  build_args    Map of build arguments: {"ARG": "value"}.
  no_cache      Disable Docker layer cache (default: false).
  push          Push image after build (default: false).
  labels        Map of image labels: {"key": "value"}.

EXAMPLES

  Basic image build:
    {"tag": "myapp:latest", "context": "."}

  Multi-stage with platform:
    {"tag": "myapp:v1", "dockerfile": "deploy/Dockerfile",
     "target": "production", "platform": "linux/amd64"}

  Build with args and push:
    {"tag": "ghcr.io/org/app:latest", "build_args": {"VERSION": "1.0"},
     "push": true, "labels": {"org.opencontainers.image.source": "https://github.com/org/app"}}

ACTIONS GENERATED

  docker-build   Runs 'docker build' with configured options.
  docker-push    Pushes the image to a registry (only when push: true).
                 Depends on docker-build.

Both actions require network access.

CAPABILITIES

  discover, plan
