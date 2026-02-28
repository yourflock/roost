#!/bin/sh
# qpkg.sh — QNAP QPKG main service control script for Roost.
# QTS calls this script with: start | stop | restart | enable | disable | status
# Must be POSIX sh.

QPKG_NAME="Roost"
QPKG_INSTALL_DIR="/usr/local/Roost"
BINARY="${QPKG_INSTALL_DIR}/roost"
CONFIG_FILE="/etc/config/roost.conf"
PID_FILE="/var/run/roost.pid"
LOG_FILE="/var/log/roost.log"
INIT_SCRIPT="/etc/init.d/Roost.sh"

# ─── Helpers ────────────────────────────────────────────────────────────────

load_config() {
    if [ -f "${CONFIG_FILE}" ]; then
        while IFS= read -r line; do
            case "$line" in
                '#'*|'') continue ;;
                *=*)
                    key="${line%%=*}"
                    val="${line#*=}"
                    export "$key"="$val"
                    ;;
            esac
        done < "${CONFIG_FILE}"
    fi
}

is_running() {
    if [ -f "${PID_FILE}" ]; then
        PID=$(cat "${PID_FILE}")
        kill -0 "$PID" 2>/dev/null
        return $?
    fi
    return 1
}

# ─── Actions ────────────────────────────────────────────────────────────────

pkg_start() {
    if is_running; then
        echo "${QPKG_NAME} is already running."
        return 0
    fi

    if [ ! -x "${BINARY}" ]; then
        echo "ERROR: Roost binary not found at ${BINARY}"
        return 1
    fi

    load_config
    mkdir -p "$(dirname "${LOG_FILE}")"

    # Start Roost in background, write PID file.
    "${BINARY}" >> "${LOG_FILE}" 2>&1 &
    echo $! > "${PID_FILE}"

    echo "${QPKG_NAME} started (PID $(cat "${PID_FILE}"))."
}

pkg_stop() {
    if ! is_running; then
        echo "${QPKG_NAME} is not running."
        return 0
    fi

    PID=$(cat "${PID_FILE}")
    kill -TERM "$PID" 2>/dev/null

    # Wait up to 15 seconds for graceful shutdown.
    i=0
    while is_running && [ "$i" -lt 15 ]; do
        sleep 1
        i=$((i + 1))
    done

    # Force kill if still running after grace period.
    is_running && kill -KILL "$PID" 2>/dev/null

    rm -f "${PID_FILE}"
    echo "${QPKG_NAME} stopped."
}

pkg_restart() {
    pkg_stop
    sleep 1
    pkg_start
}

pkg_enable() {
    /sbin/setcfg "${QPKG_NAME}" Enable TRUE -f /etc/config/qpkg.conf
    echo "${QPKG_NAME} enabled."
}

pkg_disable() {
    pkg_stop
    /sbin/setcfg "${QPKG_NAME}" Enable FALSE -f /etc/config/qpkg.conf
    echo "${QPKG_NAME} disabled."
}

pkg_status() {
    if is_running; then
        PID=$(cat "${PID_FILE}")
        echo "${QPKG_NAME} is running (PID ${PID})."
        return 0
    else
        echo "${QPKG_NAME} is stopped."
        return 1
    fi
}

# ─── Install / Uninstall ────────────────────────────────────────────────────

pkg_install() {
    echo "Installing ${QPKG_NAME}..."

    # Extract binary from package (set by QDK during build).
    if [ -f "${SYS_QPKG_DIR}/roost" ]; then
        cp "${SYS_QPKG_DIR}/roost" "${BINARY}"
        chmod +x "${BINARY}"
    fi

    # Create default config if missing.
    if [ ! -f "${CONFIG_FILE}" ]; then
        cat > "${CONFIG_FILE}" << 'EOF'
# Roost configuration — QNAP install
# Edit this file and restart Roost from App Center to apply changes.
ROOST_MODE=private
ROOST_PORT=7979
ROOST_SECRET_KEY=
POSTGRES_HOST=localhost
POSTGRES_PORT=5432
POSTGRES_DB=roost
POSTGRES_USER=roost
POSTGRES_PASSWORD=
REDIS_URL=redis://localhost:6379/0
MEDIA_PATH=/share/Multimedia
RECORDINGS_PATH=/share/Recordings/Roost
EOF
        chmod 600 "${CONFIG_FILE}"
    fi

    # Register QTS service.
    /sbin/setcfg "${QPKG_NAME}" Enable TRUE -f /etc/config/qpkg.conf
    /sbin/setcfg "${QPKG_NAME}" RC_Number "${QPKG_RC_NUM:-101}" -f /etc/config/qpkg.conf
    /sbin/setcfg "${QPKG_NAME}" Shell "${INIT_SCRIPT}" -f /etc/config/qpkg.conf

    echo "${QPKG_NAME} installed."
}

pkg_uninstall() {
    echo "Uninstalling ${QPKG_NAME}..."
    pkg_stop

    rm -rf "${QPKG_INSTALL_DIR}"
    rm -f "${PID_FILE}"
    # Preserve config file — user may reinstall later.

    /sbin/setcfg "${QPKG_NAME}" Enable FALSE -f /etc/config/qpkg.conf
    echo "${QPKG_NAME} uninstalled. Config preserved at ${CONFIG_FILE}."
}

# ─── Dispatch ────────────────────────────────────────────────────────────────

case "$1" in
    start)     pkg_start ;;
    stop)      pkg_stop ;;
    restart)   pkg_restart ;;
    enable)    pkg_enable ;;
    disable)   pkg_disable ;;
    status)    pkg_status ;;
    install)   pkg_install ;;
    uninstall) pkg_uninstall ;;
    *)
        echo "Usage: $0 {start|stop|restart|enable|disable|status}"
        exit 1
        ;;
esac
