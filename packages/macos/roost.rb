# Homebrew formula for Roost — self-hosted media backend for Owl.
#
# Usage:
#   brew install unyeco/tap/roost
#
# This formula is maintained in the homebrew-roost tap:
#   https://github.com/unyeco/homebrew-roost
#
# The source of truth for the formula lives here (packages/macos/roost.rb)
# and is synced to the tap on release.

class Roost < Formula
  desc "Self-hosted media backend for Owl — movies, TV, live TV, music, podcasts, games"
  homepage "https://github.com/unyeco/roost"
  version "1.0.0"

  on_macos do
    on_arm do
      url "https://github.com/unyeco/roost/releases/download/v#{version}/roost-#{version}-darwin-arm64.tar.gz"
      sha256 "TODO_SHA256_DARWIN_ARM64"
    end
    on_intel do
      url "https://github.com/unyeco/roost/releases/download/v#{version}/roost-#{version}-darwin-amd64.tar.gz"
      sha256 "TODO_SHA256_DARWIN_AMD64"
    end
  end

  def install
    bin.install "roost"

    # Install default config to etc
    etc.install "roost.env.example" => "roost/roost.env.example"

    # Install launchd plist for auto-start
    (prefix/"homebrew.mxcl.roost.plist").write plist_content
  end

  def plist_content
    <<~EOS
      <?xml version="1.0" encoding="UTF-8"?>
      <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
      <plist version="1.0">
        <dict>
          <key>Label</key>
          <string>homebrew.mxcl.roost</string>
          <key>ProgramArguments</key>
          <array>
            <string>#{opt_bin}/roost</string>
          </array>
          <key>EnvironmentVariables</key>
          <dict>
            <key>ROOST_ENV_FILE</key>
            <string>#{etc}/roost/roost.env</string>
          </dict>
          <key>RunAtLoad</key>
          <true/>
          <key>KeepAlive</key>
          <true/>
          <key>StandardOutPath</key>
          <string>#{var}/log/roost.log</string>
          <key>StandardErrorPath</key>
          <string>#{var}/log/roost.log</string>
        </dict>
      </plist>
    EOS
  end

  service do
    run [opt_bin/"roost"]
    keep_alive true
    log_path var/"log/roost.log"
    error_log_path var/"log/roost.log"
  end

  def caveats
    <<~EOS
      Roost requires PostgreSQL. Install it with:
        brew install postgresql@16
        brew services start postgresql@16

      Copy and edit the example config:
        cp #{etc}/roost/roost.env.example #{etc}/roost/roost.env
        nano #{etc}/roost/roost.env

      Start Roost:
        brew services start roost

      Roost listens on http://localhost:8080 by default.
    EOS
  end

  test do
    system "#{bin}/roost", "--version"
  end
end
