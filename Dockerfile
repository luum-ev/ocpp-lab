# Build: docker build -t ocpp-lab .
# Run (docker or podman), mounting your fleet file:
#   docker run -v $PWD/fleet.yaml:/etc/ocpp-lab/fleet.yaml \
#     -e OCPP_LAB_CSMS=ws://host.docker.internal:9000/ocpp \
#     -p 8887:8887 ghcr.io/luum-ev/ocpp-lab
# In Kubernetes the same file arrives as a ConfigMap mounted at the same path.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /ocpp-lab ./cmd/ocpp-lab

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ocpp-lab /ocpp-lab
# The container contract: configuration comes from the environment, the fleet
# file comes from a mount. Defaults below are overridable at run time.
ENV OCPP_LAB_FLEET=/etc/ocpp-lab/fleet.yaml \
    OCPP_LAB_LISTEN=:8887
EXPOSE 8887
USER nonroot
ENTRYPOINT ["/ocpp-lab"]
CMD ["serve"]
