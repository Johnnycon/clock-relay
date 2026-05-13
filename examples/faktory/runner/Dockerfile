FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/faktory-runner .

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/faktory-runner /usr/local/bin/faktory-runner
EXPOSE 8090
CMD ["faktory-runner"]
