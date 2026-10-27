# Builds ONLY the generic platform program. Cores are separate processes/containers.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY contracts ./contracts
COPY engine ./engine
RUN CGO_ENABLED=0 go build -trimpath -o /platform ./engine/cmd/platform

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /platform /platform
# config/ and contracts/ are mounted at run time (see docker-compose.yml)
EXPOSE 8080
ENTRYPOINT ["/platform", "-config", "/config/platform.json", "-contracts", "/contracts", "-listen", "0.0.0.0:8080"]
