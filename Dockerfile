FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bench ./cmd/bench

FROM golang:1.24
RUN apt-get update && apt-get install -y --no-install-recommends git python3 python3-pytest && rm -rf /var/lib/apt/lists/*
COPY --from=build /bench /usr/local/bin/bench
WORKDIR /repo
ENTRYPOINT ["bench"]
