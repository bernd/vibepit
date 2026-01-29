FROM ubuntu:25.10

ENV CODE_UID=1000
ENV CODE_GID=1000
ENV CODE_USER=code
ENV CODE_GROUP=code

RUN userdel -r ubuntu

RUN groupadd -g $CODE_GID $CODE_GROUP \
  && useradd -u $CODE_UID -s /bin/bash -d /home/$CODE_USER \
    -g $CODE_GROUP -c "Code" -m \
    $CODE_USER \
  && chown -R ${CODE_UID}:${CODE_GID} /home/$CODE_USER \
  && install -d -m 0700 -o $CODE_USER -g $CODE_GROUP /src

RUN apt-get update \
  && apt-get dist-upgrade -V -y \
  && apt-get install -y --no-install-recommends \
    7zip \
    build-essential \
    bzip2 \
    ca-certificates \
    cabextract \
    curl \
    cpio \
    file \
    git \
    htop \
    info \
    ipcalc \
    jq \
    less \
    locales \
    man-db \
    manpages \
    ncat \
    netcat-openbsd \
    openssl \
    p7zip \
    passwdqc \
    pigz \
    psmisc \
    pv \
    pwgen \
    python3 \
    ripgrep \
    rsync \
    shellcheck \
    sqlite3 \
    strace \
    tree \
    unzip \
    vim \
    wget \
    zip \
    zstd \
  && locale-gen en_US.UTF-8 \
  && apt-get autoremove --purge -y \
  && apt-get clean \
  && rm -rf /tmp/* \
  && rm -rf /var/cache/debconf/* \
  && rm -rf /var/lib/apt/lists/* \
  && rm -rf /var/log/*

RUN install -d  -m 0755 "/usr/local/bin"
COPY bin "/usr/local/bin"

RUN mv /home/$CODE_USER /home/.${CODE_USER}.template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER

COPY entrypoint.sh /entrypoint.sh

USER $CODE_UID

ENTRYPOINT ["/entrypoint.sh"]
