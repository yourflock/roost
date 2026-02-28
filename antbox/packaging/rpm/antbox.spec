Name:           antbox
Version:        1.0.0
Release:        1%{?dist}
Summary:        USB tuner live TV daemon for Owl
License:        MIT
URL:            https://github.com/yourflock/owl
Source0:        antbox-%{version}.tar.gz

BuildRequires:  golang >= 1.22
Requires:       systemd

%description
AntBox turns any Linux machine with a USB DVB TV tuner into a live TV
source for Owl media app. It discovers USB tuner hardware, captures
MPEG-TS streams, and delivers them to Owl backend via gRPC.

%prep
%autosetup

%build
CGO_ENABLED=0 go build -ldflags "-X antbox/daemon.Version=%{version} -s -w" -o antboxd .

%install
install -D -m 0755 antboxd %{buildroot}/usr/bin/antboxd
install -D -m 0644 packaging/rpm/antbox.service %{buildroot}/lib/systemd/system/antbox.service
install -D -m 0644 configs/antbox.yaml.example %{buildroot}/etc/antbox/antbox.yaml

%pre
getent group antbox >/dev/null || groupadd -r antbox
getent passwd antbox >/dev/null || useradd -r -g antbox -s /sbin/nologin antbox

%post
systemctl daemon-reload
systemctl enable antbox
echo "AntBox v%{version} installed. Start with: systemctl start antbox"

%preun
systemctl stop antbox || true
systemctl disable antbox || true

%postun
systemctl daemon-reload

%files
/usr/bin/antboxd
/lib/systemd/system/antbox.service
%config(noreplace) /etc/antbox/antbox.yaml

%changelog
* Mon Feb 24 2026 Flock <support@yourflock.org> - 1.0.0-1
- Initial stable release
