FROM chromedp/headless-shell

# Install image optimization tools
RUN apt-get -y update --fix-missing && \
    apt-get upgrade -y && \
    apt-get --no-install-recommends install -y optipng jpegoptim && \
    rm -rf /var/lib/apt/lists/*

ADD wrp /wrp
ENTRYPOINT ["/wrp"]
ENV PATH="/headless-shell:${PATH}"
LABEL maintainer="as@tenoware.com"
