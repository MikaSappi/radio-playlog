FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/ui-api .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ui-api /ui-api
COPY configuration.json /configuration.json
ENV UI_API_CONFIG=/configuration.json
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ui-api"]
