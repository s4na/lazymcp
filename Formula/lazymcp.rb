class Lazymcp < Formula
  desc "Lightweight MCP proxy/router that lazily spawns backend MCP servers"
  homepage "https://github.com/s4na/lazymcp"
  url "https://github.com/s4na/lazymcp.git", branch: "main"
  version "0.0.1"

  depends_on "go" => :build

  def install
    system "go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin/"lazymcp", "./cmd/lazymcp"
  end

  test do
    assert_match "Lazy MCP proxy/router", shell_output("#{bin}/lazymcp --help")
  end
end
