{
  description = "Tincan durable agent messages and notifications";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/release-25.11";
  inputs.herdr.url = "github:herdrdev/herdr/v0.9.3";

  outputs = { self, nixpkgs, herdr }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forEachSystem = function:
        nixpkgs.lib.genAttrs systems (system: function (import nixpkgs { inherit system; }));
      golinesFlags = "-m 100 --no-reformat-tags --base-formatter=gofmt";
      goSource = pkgs: pkgs.lib.fileset.toSource {
        root = ./.;
        fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum ./cmd ./pkg ];
      };
      buildTransport = pkgs: pkgs.buildGoModule {
        pname = "tincan-command";
        version = "0.1.0";
        src = goSource pkgs;
        go = pkgs.go_1_25;
        vendorHash = "sha256-Fx5aHPDPjwe9iomWBJa3yMcuIHx4W2CtHwMg1q62rDI=";
        subPackages = [ "cmd/tincan" ];
        checkPhase = ''
          runHook preCheck
          go list -buildvcs=false ./... > tincan-packages
          while IFS= read -r package; do
            case "$package" in
              github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
              *)
                printf 'Tincan repository contains a non-transport package: %s\n' "$package" >&2
                exit 1
                ;;
            esac
          done < tincan-packages
          go list -deps ./cmd/tincan > tincan-dependencies
          while IFS= read -r dependency; do
            case "$dependency" in
              github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
              github.com/roman/tincan/*|github.com/roman/concierge|github.com/roman/concierge/*)
                printf 'Forbidden Tincan dependency: %s\n' "$dependency" >&2
                exit 1
                ;;
            esac
          done < tincan-dependencies
          go test ./...
          runHook postCheck
        '';
      };
    in
    {
      packages = forEachSystem (pkgs:
        let
          command = buildTransport pkgs;
          herdrPackage = herdr.packages.${pkgs.stdenv.hostPlatform.system}.default;
          tincan = pkgs.runCommand "tincan-0.1.0" {
            nativeBuildInputs = [ pkgs.makeWrapper ];
          } ''
            install -Dm755 ${command}/bin/tincan "$out/bin/tincan"
            wrapProgram "$out/bin/tincan" --prefix PATH : ${herdrPackage}/bin
          '';
        in
        {
          default = tincan;
          inherit tincan;
        });

      formatter = forEachSystem (pkgs: pkgs.writeShellApplication {
        name = "tincan-format";
        runtimeInputs = [ pkgs.golines pkgs.go_1_25 ];
        text = ''
          if [ "$#" -eq 0 ]; then
            set -- .
          fi
          exec golines ${golinesFlags} -w "$@"
        '';
      });

      checks = forEachSystem (pkgs:
        let
          packages = self.packages.${pkgs.stdenv.hostPlatform.system};
        in
        {
          inherit (packages) tincan;
          tincan-smoke = pkgs.runCommand "tincan-smoke" { } ''
            unset TINCAN_HOME HERDR_SESSION HERDR_SOCKET_PATH HERDR_WORKSPACE_ID HERDR_TAB_ID HERDR_PANE_ID
            export PATH=
            test -x ${packages.tincan}/bin/tincan
            test "$(${packages.tincan}/bin/tincan --version)" = 0.1.0
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox send smoke --body 'Packaged message' > sent
            ${pkgs.gnugrep}/bin/grep -q '^id: ' sent
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox list smoke > pending
            ${pkgs.gnugrep}/bin/grep -q '^count: 1$' pending
            ${packages.tincan}/bin/tincan notify --help >/dev/null
            : > "$out"
          '';
          go-source = pkgs.runCommand "tincan-go-source" { src = goSource pkgs; } ''
            test -e "$src/go.mod"
            test -e "$src/cmd/tincan"
            test -e "$src/pkg/inbox"
            test ! -e "$src/cmd/concierge"
            test ! -e "$src/pkg/taskops"
            test ! -e "$src/README.md"
            touch "$out"
          '';
          go-format = pkgs.runCommand "tincan-go-format" {
            nativeBuildInputs = [ pkgs.golines pkgs.go_1_25 ];
            src = goSource pkgs;
          } ''
            cd "$src"
            unformatted="$(golines ${golinesFlags} -l .)"
            if [ -n "$unformatted" ]; then
              printf 'Go files need formatting:\n%s\n' "$unformatted" >&2
              exit 1
            fi
            touch "$out"
          '';
          go-tests = buildTransport pkgs;
        });

      devShells = forEachSystem (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go_1_25 pkgs.gopls pkgs.golines herdr.packages.${pkgs.stdenv.hostPlatform.system}.default ];
        };
      });
    };
}
