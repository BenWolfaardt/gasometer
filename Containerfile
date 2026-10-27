FROM docker.io/library/golang:1.26.10-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# avalanchego needs cgo (blst, libevm's secp256k1); link static so the distroless static image still runs it.
RUN CGO_ENABLED=1 go build -tags netgo,osusergo -trimpath \
    -ldflags='-s -w -linkmode external -extldflags -static' -o /out/gasometer ./cmd/gasometer

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/gasometer /gasometer
COPY configs/gasometer.yml /etc/gasometer/gasometer.yml
EXPOSE 9090
ENTRYPOINT ["/gasometer"]
CMD ["--config-file", "/etc/gasometer/gasometer.yml"]
