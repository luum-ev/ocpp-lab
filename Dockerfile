FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /ocpp-lab ./cmd/ocpp-lab

FROM gcr.io/distroless/static-debian12
COPY --from=build /ocpp-lab /ocpp-lab
EXPOSE 8887
ENTRYPOINT ["/ocpp-lab"]
CMD ["serve", "--fleet", "/fleet.yaml"]
