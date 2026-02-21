FROM ubuntu:25.10

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
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
    fzf \
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
    tmux \
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

ARG CODE_UID=1000
ARG CODE_GID=1000

ENV CODE_UID=${CODE_UID}
ENV CODE_GID=${CODE_GID}
ENV CODE_USER=code
ENV CODE_GROUP=code

RUN userdel -r ubuntu \
  && groupdel -f "$(getent group 20 | cut -d : -f 1)"

RUN groupadd -g $CODE_GID $CODE_GROUP \
  && useradd -l -u $CODE_UID -s /bin/bash -d /home/$CODE_USER \
    -g $CODE_GROUP -c "Code" -m \
    $CODE_USER \
  && chown -R ${CODE_UID}:${CODE_GID} /home/$CODE_USER

# Homebrew on Linux needs /home/linuxbrew writable.
RUN install -d -m 0755 "/usr/local/bin" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/${CODE_USER}/.local" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/${CODE_USER}/.local/bin" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home" \
  && install -d -o ${CODE_UID} -g ${CODE_GID} -m 0755 "/home/linuxbrew"

COPY bin /usr/local/bin
COPY config/profile /etc/profile.d/999-vibepit.sh

RUN install -d -o root -g root -m 0755 /etc/vibepit \
  && install -d -o root -g root -m 0755 /opt/vibepit
COPY config/bashrc /etc/vibepit/bashrc
COPY config/tmux.conf /etc/vibepit/tmux.conf
COPY lib.sh /etc/vibepit/lib.sh

COPY config/bashrc.tail /tmp/bashrc.tail
RUN cat /tmp/bashrc.tail >> /home/$CODE_USER/.bashrc && rm -f /tmp/bashrc.tail

RUN mv /home/$CODE_USER /opt/vibepit/home-template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER

COPY entrypoint-lib.sh /etc/vibepit/entrypoint-lib.sh
COPY entrypoint.sh /entrypoint.sh

USER $CODE_UID

ENTRYPOINT ["/entrypoint.sh"]
