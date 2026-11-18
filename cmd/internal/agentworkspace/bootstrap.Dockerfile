# Image of the disposable container that clones a --clone-into-volume
# workspace into its content volume (see bootstrap.go). Follows the recipe of
# the VS Code Dev Containers extension's volume bootstrap image.
# The release workflow publishes it as
# ghcr.io/<owner>/devsy-volume-bootstrap:<first 12 hex of sha256(this file)>.
FROM mcr.microsoft.com/devcontainers/base:4.0.10-alpine3.23@sha256:8779d14dfb53154e3e5a4909d98f2c4d6be7374f9ec67965502c36331b0bc42d
RUN apk add --no-cache git-lfs nodejs python3 npm make g++ docker-cli docker-cli-buildx docker-cli-compose
