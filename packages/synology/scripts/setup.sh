#!/bin/sh
# setup.sh — Applies wizard-collected values to the Roost config file.
# Called by DSM after the install wizard completes.
# Wizard values are available as environment variables: wizard_<key>

CONFIG_FILE="/etc/roost/roost.env"

# Apply wizard values if provided.
if [ -n "$wizard_media_path" ]; then
    sed -i "s|^MEDIA_PATH=.*|MEDIA_PATH=${wizard_media_path}|" "${CONFIG_FILE}"
fi

if [ -n "$wizard_port" ]; then
    sed -i "s|^ROOST_PORT=.*|ROOST_PORT=${wizard_port}|" "${CONFIG_FILE}"
fi

if [ -n "$wizard_mode" ]; then
    sed -i "s|^ROOST_MODE=.*|ROOST_MODE=${wizard_mode}|" "${CONFIG_FILE}"
fi

if [ -n "$wizard_secret_key" ]; then
    sed -i "s|^ROOST_SECRET_KEY=.*|ROOST_SECRET_KEY=${wizard_secret_key}|" "${CONFIG_FILE}"
fi

exit 0
