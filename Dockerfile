FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hindcast ./cmd/hindcast

# Node is in the runtime image because most MCP servers are started with npx.
FROM node:22-alpine
RUN adduser -D -u 10001 hindcast
COPY --from=build /out/hindcast /usr/local/bin/hindcast
USER hindcast
WORKDIR /home/hindcast
EXPOSE 9095
ENTRYPOINT ["hindcast"]
CMD ["serve", "-c", "/etc/hindcast/hindcast.yaml"]
