FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o server .

FROM alpine:latest
WORKDIR /app
COPY --from=build /app/server .
COPY --from=build /app/index.html .
EXPOSE 8080
CMD ["./server"]
