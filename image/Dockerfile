FROM ubuntu:25.10

ARG NVM_VERSION=0.40.3

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
    build-essential \
    ca-certificates \
    curl \
    git \
    jq \
    less \
    man-db \
    pwgen \
    ripgrep \
    rsync \
    sqlite3 \
    unzip \
    vim \
    wget \
  && apt-get autoremove --purge -y \
  && apt-get clean \
  && rm -rf /tmp/* \
  && rm -rf /var/cache/debconf/* \
  && rm -rf /var/lib/apt/lists/* \
  && rm -rf /var/log/*

USER $CODE_UID

RUN install -d -m 0700 "/home/$CODE_USER/bin"
COPY bin "/home/$CODE_USER/bin"

USER 0

RUN mv /home/$CODE_USER /home/.${CODE_USER}.template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER

WORKDIR /src

RUN rm -rf /tmp/*

COPY entrypoint.sh /entrypoint.sh

USER $CODE_UID

ENTRYPOINT ["/entrypoint.sh"]
