FROM alpine:3.22
RUN apk add --no-cache git bash openssh-keygen gettext-envsubst ca-certificates tzdata
RUN adduser -D -u 1000 git
COPY gitea /usr/local/bin/gitea
USER git
ENTRYPOINT ["/usr/local/bin/gitea"]
CMD ["web"]
