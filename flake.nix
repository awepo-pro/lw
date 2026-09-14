# Nix flake for lw.
#
#   nix build .#lw          build from this working tree
#   nix run .#lw -- --help  run it without installing
#   nix profile install .#lw
#
# The build is the same one `make build` and goreleaser run: CGO_ENABLED=0,
# `main.version` stamped, `./cmd/lw` as the entry point. One static binary —
# the nix output is no different from the release tarball's.
#
# NOTE ON `vendorHash`: buildGoModule pins the Go module tree by content
# hash. That hash can only be computed by a nix evaluation (it is a NAR hash
# of the vendor tree nix constructs from go.mod/go.sum), and no nix evaluator
# is available in this build's sandbox, so the value below is `lib.fakeHash`.
# The first `nix build` fails with the real hash in its output; paste it in:
#
#   nix build .#lw 2>&1 | grep 'got:'    # → sha256-…
#
# then rebuild. This is the one line of this file that has never been
# executed; see the S6-T5 run report.
{
  description = "lw — compiles immutable sources into a reviewable markdown wiki; the agent gets no filesystem verbs";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # The four targets goreleaser builds, minus darwin/amd64's escalation
      # suffix: same matrix, expressed the way nixpkgs spells it.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems =
        f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "1.0.0"; # bump with the release tag; goreleaser stamps its own
    in
    {
      packages = forAllSystems (
        pkgs:
        let
          lw = pkgs.buildGoModule {
            pname = "lw";
            inherit version;
            src = self;

            # Pinned from the first real build (2026-09-14); refresh it whenever go.sum changes — see the note at the top of this file.
            vendorHash = "sha256-FFt7SdPmyKVuz7NbhIsyya7tuxPx9uiMQCEEF6hJ6+I=";

            # internal/stage's TestUnifiedAppliesCleanly validates generated
            # diffs with `git apply`; the build sandbox has no git otherwise.
            nativeCheckInputs = [ pkgs.git ];

            # go.mod declares `toolchain go1.27.0`; without this the build
            # would try to download that toolchain from the network, which a
            # sandboxed nix build cannot do. nixpkgs' default Go must be at
            # least go.mod's `go 1.25.8` floor for this to work.
            env = {
              GOTOOLCHAIN = "local";
              CGO_ENABLED = "0";
            };

            ldflags = [
              "-s"
              "-w"
              "-X main.version=v${version}"
            ];

            meta = with nixpkgs.lib; {
              description = "Compiles immutable sources into a reviewable markdown wiki";
              longDescription = ''
                lw reads immutable sources and proposes an interlinked markdown
                wiki as a git-commit-shaped changeset. The agent has no
                filesystem verbs — its only mutation path is a set of
                validating, transactional staging tools — and nothing lands
                until a human reviews it hunk by hunk.
              '';
              homepage = "https://github.com/awepo-pro/lw";
              license = licenses.mit;
              mainProgram = "lw";
              platforms = systems;
            };
          };
        in
        {
          inherit lw;
          default = lw;
        }
      );

      apps = forAllSystems (system: rec {
        lw = {
          type = "app";
          program = "${self.packages.${system}.lw}/bin/lw";
        };
        default = lw;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.goreleaser
          ];
        };
      });
    };
}
