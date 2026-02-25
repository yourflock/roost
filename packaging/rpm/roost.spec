# roost.spec — RPM spec file for Roost.
# Builds the Roost self-hosted media backend RPM package.
#
# Build:
#   ./packaging/rpm/build.sh [version]
#
# Or manually:
#   rpmbuild -bb packaging/rpm/roost.spec --define "version 1.0.0"

Name:           roost
Version:        1.0.0
Release:        1%{?dist}
Summary:        Self-hosted media backend for Owl
License:        MIT
URL:            https://github.com/yourflock/roost
Source0:        roost-%{version}-linux-amd64.tar.gz
BuildArch:      x86_64

# Runtime dependencies.
Requires:       systemd
Recommends:     postgresql-server, redis

%description
Roost is an open-source media backend that powers the Owl media player.
It handles Live TV (DVR, EPG), VOD, music, podcasts, and games in a
single self-contained service.

Connect Roost to the Owl app and your content appears in one unified
library across all your devices.

Roost runs in private mode by default (no billing required).

%prep
%autosetup -n roost-%{version}

%install
rm -rf %{buildroot}

# Binary
install -D -m 755 roost %{buildroot}%{_bindir}/roost

# Config
install -D -m 640 packaging/rpm/SOURCES/roost.env %{buildroot}%{_sysconfdir}/roost/roost.env

# systemd unit
install -D -m 644 packaging/rpm/SOURCES/roost.service %{buildroot}%{_unitdir}/roost.service

# Log directory
install -d -m 755 %{buildroot}%{_localstatedir}/log/roost

# Data directory
install -d -m 755 %{buildroot}%{_localstatedir}/lib/roost

%pre
# Create the roost system user if it does not exist.
if ! id roost >/dev/null 2>&1; then
    useradd --system \
        --no-create-home \
        --shell /sbin/nologin \
        --comment "Roost media service" \
        roost
fi

%post
# Set ownership on config file.
chown root:roost %{_sysconfdir}/roost/roost.env || true
chmod 640 %{_sysconfdir}/roost/roost.env || true

# Set ownership on log and data directories.
chown roost:roost %{_localstatedir}/log/roost || true
chown roost:roost %{_localstatedir}/lib/roost || true

# Enable the systemd service.
%systemd_post roost.service

echo ""
echo "Roost installed successfully."
echo ""
echo "Before starting Roost, edit %{_sysconfdir}/roost/roost.env and set:"
echo "  ROOST_SECRET_KEY   — generate with: openssl rand -hex 32"
echo "  POSTGRES_PASSWORD  — your PostgreSQL password"
echo ""
echo "Then start the service:"
echo "  sudo systemctl enable --now roost"
echo ""

%preun
%systemd_preun roost.service

%postun
%systemd_postun_with_restart roost.service

# On full removal: clean up the roost user.
if [ $1 -eq 0 ]; then
    if id roost >/dev/null 2>&1; then
        userdel roost || true
    fi
fi

%files
%license LICENSE
%doc README.md
%{_bindir}/roost
%config(noreplace) %{_sysconfdir}/roost/roost.env
%{_unitdir}/roost.service
%dir %attr(755, roost, roost) %{_localstatedir}/log/roost
%dir %attr(755, roost, roost) %{_localstatedir}/lib/roost

%changelog
* Mon Feb 24 2026 yourflock <hello@yourflock.com> - 1.0.0-1
- Initial release
