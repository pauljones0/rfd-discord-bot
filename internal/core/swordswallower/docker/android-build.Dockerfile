FROM gradle:8.9-jdk17

ARG ANDROID_CMDLINE_TOOLS=11076708
ENV ANDROID_HOME=/opt/android-sdk
ENV ANDROID_SDK_ROOT=/opt/android-sdk
ENV PATH="${ANDROID_HOME}/cmdline-tools/latest/bin:${ANDROID_HOME}/platform-tools:${PATH}"

USER root

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libdbus-1-3 \
        libfontconfig1 \
        libgl1 \
        libnss3 \
        libpulse0 \
        libx11-6 \
        libxcomposite1 \
        libxcursor1 \
        libxi6 \
        libxrandr2 \
        libxtst6 \
        socat \
        unzip \
        wget \
    && rm -rf /var/lib/apt/lists/*

COPY <<EOF /usr/local/bin/setup-android-sdk.sh
#!/usr/bin/env bash
set -e
if [ ! -d "\${ANDROID_HOME}/cmdline-tools/latest" ]; then
    echo "Android SDK not found in \${ANDROID_HOME}. Installing..."
    mkdir -p "\${ANDROID_HOME}/cmdline-tools"
    wget -q "https://dl.google.com/android/repository/commandlinetools-linux-${ANDROID_CMDLINE_TOOLS}_latest.zip" -O /tmp/android-tools.zip
    unzip -q /tmp/android-tools.zip -d "\${ANDROID_HOME}/cmdline-tools"
    mv "\${ANDROID_HOME}/cmdline-tools/cmdline-tools" "\${ANDROID_HOME}/cmdline-tools/latest"
    rm /tmp/android-tools.zip

    yes | sdkmanager --licenses >/dev/null
    sdkmanager \
        "emulator" \
        "platform-tools" \
        "platforms;android-24" \
        "platforms;android-35" \
        "build-tools;35.0.0" \
        "system-images;android-24;google_apis;x86_64"
else
    echo "Android SDK already present in \${ANDROID_HOME}."
fi
EOF

RUN chmod +x /usr/local/bin/setup-android-sdk.sh

WORKDIR /workspace
