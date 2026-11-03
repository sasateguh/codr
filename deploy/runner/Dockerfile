# codr-runner image (D89). The binary is built on the host by deploy/deploy.sh like codr's. It needs git (fetch the
# pushed commit) and the docker CLI (start one locked-down container per job on the host's daemon through the mounted
# socket). Runs as uid 1000; deploy.sh adds the socket's group.
FROM alpine:3.22
# apk upgrade: take fixed Alpine packages even when the cached base image is older (Perisai CD-01, D107).
RUN apk upgrade --no-cache && apk add --no-cache git docker-cli ca-certificates tzdata \
 && adduser -D -u 1000 -h /home/runner runner \
 && mkdir -p /etc/codr-runner && chown runner:runner /etc/codr-runner
COPY codr-runner /usr/local/bin/codr-runner
USER runner
WORKDIR /home/runner
ENV CODR_RUNNER_CONFIG=/etc/codr-runner/runner.toml
EXPOSE 3200
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:3200/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/codr-runner"]
CMD ["serve"]
