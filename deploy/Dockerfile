# Codr runtime image. The binary is built first by scripts/build-release.sh (CGO_ENABLED=0, pure-Go
# SQLite), copied next to this file as ./codr, so this image needs no Go toolchain: alpine + git + git-daemon (Alpine ships
# git-http-backend there, not in git — measured 14 Oct 2026: every HTTPS clone was a 500) +
# openssh-client (ssh:// mirror sources). Runs as uid 1000 to match the bind-mounted data dir.
FROM alpine:3.22
# apk upgrade: take fixed Alpine packages even when the cached base image is older (D107).
RUN apk upgrade --no-cache && apk add --no-cache git git-daemon openssh-client ca-certificates tzdata \
 && adduser -D -u 1000 -h /home/codr codr \
 && mkdir -p /data /etc/codr && chown codr:codr /data /etc/codr
COPY codr /usr/local/bin/codr
USER codr
WORKDIR /home/codr
ENV CODR_CONFIG=/etc/codr/config.toml
EXPOSE 3000 2222
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:3000/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/codr"]
CMD ["serve"]
