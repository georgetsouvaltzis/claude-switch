# Template for Formula/csw.rb in georgetsouvaltzis/homebrew-tap. The release
# workflow fills in VERSION and SHA256 and pushes it there.
class Csw < Formula
  desc "Switch between Claude subscriptions in Claude Code"
  homepage "https://github.com/georgetsouvaltzis/claude-switch"
  url "https://github.com/georgetsouvaltzis/claude-switch/archive/refs/tags/VERSION.tar.gz"
  sha256 "SHA256"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/csw"
  end

  test do
    assert_match "csw #{version}", shell_output("#{bin}/csw version")
  end
end
