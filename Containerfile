FROM docker.io/library/golang:1.26.10-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/gasometer ./cmd/gasometer

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/gasometer /gasometer
COPY configs/gasometer.yml /etc/gasometer/gasometer.yml
EXPOSE 9090
ENTRYPOINT ["/gasometer"]
CMD ["--config-file", "/etc/gasometer/gasometer.yml"]
