# Template for Formula/csw.rb in georgetsouvaltzis/homebrew-tap. The release
# workflow fills in VERSION and the SHA_* checksums from GoReleaser's
# checksums.txt, and pushes it there.
class Csw < Formula
  desc "Switch between Claude subscriptions in Claude Code"
  homepage "https://github.com/georgetsouvaltzis/claude-switch"
  version "VERSION"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/georgetsouvaltzis/claude-switch/releases/download/VERSION/csw_VERSION_darwin_arm64.tar.gz"
      sha256 "SHA_DARWIN_ARM64"
    end
    on_intel do
      url "https://github.com/georgetsouvaltzis/claude-switch/releases/download/VERSION/csw_VERSION_darwin_amd64.tar.gz"
      sha256 "SHA_DARWIN_AMD64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/georgetsouvaltzis/claude-switch/releases/download/VERSION/csw_VERSION_linux_arm64.tar.gz"
      sha256 "SHA_LINUX_ARM64"
    end
    on_intel do
      url "https://github.com/georgetsouvaltzis/claude-switch/releases/download/VERSION/csw_VERSION_linux_amd64.tar.gz"
      sha256 "SHA_LINUX_AMD64"
    end
  end

  def install
    bin.install "csw"
  end

  test do
    assert_match "csw #{version}", shell_output("#{bin}/csw version")
  end
end
