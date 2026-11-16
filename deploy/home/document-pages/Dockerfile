FROM debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a
ARG DEBIAN_MIRROR=deb.debian.org
RUN sed -i "s|deb.debian.org|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends python3 python3-uno libreoffice-writer libreoffice-impress fonts-liberation2 fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /opt/new-api-document-pages
COPY worker.py /opt/new-api-document-pages/worker.py
USER 65532:65532
ENV HOME=/work LANG=C.UTF-8 LC_ALL=C.UTF-8 SAL_USE_VCLPLUGIN=svp
WORKDIR /work
ENTRYPOINT ["python3", "/opt/new-api-document-pages/worker.py"]
