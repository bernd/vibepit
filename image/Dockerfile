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

RUN curl -fsSL "https://godeb.s3.amazonaws.com/godeb-amd64.tar.gz" | \
  tar -C "/usr/local/bin" -xzf - "godeb" \
  && (cd /tmp && godeb install "$(godeb list | tail -1)")

USER $CODE_UID

RUN curl -fsSL https://astral.sh/uv/install.sh | bash
RUN curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v${NVM_VERSION}/install.sh | bash
RUN curl -fsSL https://deno.land/install.sh | bash
RUN curl -fsSL https://bun.sh/install | bash

RUN /bin/bash --login -c 'source "$HOME/.nvm/nvm.sh" && nvm install --lts' \
  && /bin/bash --login -c 'source "$HOME/.nvm/nvm.sh" && nvm install-latest-npm' \
  && /bin/bash --login -c 'source "$HOME/.nvm/nvm.sh" && (cd $HOME/.local && npm install @anthropic-ai/claude-code)'

RUN /bin/bash --login -c '$HOME/.local/bin/uv python install --default'
RUN /bin/bash --login -c 'echo "fund=false" > $HOME/.npmrc'

RUN mkdir -p "/home/$CODE_USER/bin"
COPY bin/claude "/home/$CODE_USER/bin/claude"
COPY bin/amp "/home/$CODE_USER/bin/amp"

USER 0

RUN mv /home/$CODE_USER /home/.${CODE_USER}.template \
  && install -d 0750 -o $CODE_USER -g $CODE_GROUP /home/$CODE_USER

WORKDIR /src

RUN rm -rf /tmp/*

COPY entrypoint.sh /entrypoint.sh

USER $CODE_UID

ENTRYPOINT ["/entrypoint.sh"]
