FROM chromedp/headless-shell

ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update -y \
  && apt-get -y install fonts-arphic-ukai fonts-arphic-uming fonts-ipafont-mincho fonts-ipafont-gothic fonts-unfonts-core netpbm optipng \
  && apt-get -y clean && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

ADD wrp /wrp
ENTRYPOINT ["/wrp"]
ENV PATH="/headless-shell:${PATH}"
LABEL maintainer="alb42@web.de"
