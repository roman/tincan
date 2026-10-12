{
  description = "Tincan communication and Concierge supervisor tools";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/release-25.11";
  inputs.herdr.url = "github:herdrdev/herdr/v0.9.3";

  outputs = { self, nixpkgs, herdr }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forEachSystem = function:
        nixpkgs.lib.genAttrs systems (system: function (import nixpkgs { inherit system; }));
      golinesFlags = "-m 100 --no-reformat-tags --base-formatter=gofmt";
      goSource = pkgs: pkgs.lib.fileset.toSource {
        root = ./.;
        fileset = pkgs.lib.fileset.unions [ ./go.mod ./go.sum ./cmd ./pkg ];
      };
      goModule = pkgs: {
        version = "0.1.0";
        src = goSource pkgs;
        go = pkgs.go_1_25;
        vendorHash = "sha256-dnZtAeufL7NxseT95FGiPCvq6oGp6eUO7/sIQEHNRa0=";
      };
      buildCommands = pkgs:
        let tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
        in pkgs.buildGoModule ((goModule pkgs) // {
          pname = "tincan-commands";
          subPackages = [ "cmd/..." ];
          checkPhase = ''
            runHook preCheck
            export PATH=${tasksAxi}/bin:$PATH
            go list -deps ./cmd/tincan > tincan-dependencies
            while IFS= read -r dependency; do
              case "$dependency" in
                github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
                github.com/roman/tincan/*)
                  printf 'Forbidden Tincan dependency: %s\n' "$dependency" >&2
                  exit 1
                  ;;
              esac
            done < tincan-dependencies
            go test ./...
            runHook postCheck
          '';
        });
    in
    {
      packages = forEachSystem (pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          herdrPackage = herdr.packages.${system}.default;
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
          allCommands = buildCommands pkgs;
          mkProgram = { name, runtime ? null }: pkgs.runCommand "${name}-0.1.0" {
            nativeBuildInputs = pkgs.lib.optionals (runtime != null) [ pkgs.makeWrapper ];
            disallowedRequisites = pkgs.lib.optionals (runtime == null) [ herdrPackage ];
          } ''
            install -Dm755 ${allCommands}/bin/${name} "$out/bin/${name}"
            ${pkgs.lib.optionalString (runtime != null) ''
              wrapProgram "$out/bin/${name}" --prefix PATH : ${runtime}/bin
            ''}
          '';
          tincan-command = mkProgram { name = "tincan"; runtime = herdrPackage; };
          tincan-inbox = mkProgram { name = "tincan-inbox"; };
          tincan-notify = mkProgram { name = "tincan-notify"; runtime = herdrPackage; };
          tincan-task-send = mkProgram { name = "tincan-task-send"; runtime = herdrPackage; };
          tincan-task-reply = mkProgram { name = "tincan-task-reply"; };
          tincan-task-receive = mkProgram { name = "tincan-task-receive"; };
          concierge-brief = mkProgram { name = "concierge-brief"; runtime = tasksAxi; };
          concierge-primary = mkProgram { name = "concierge-primary"; };
          concierge-worker = mkProgram { name = "concierge-worker"; };
          concierge-backlog = mkProgram { name = "concierge-backlog"; runtime = tasksAxi; };
          concierge-task = mkProgram { name = "concierge-task"; runtime = herdrPackage; };
          concierge-migrate = mkProgram { name = "concierge-migrate"; };
          concierge-control = mkProgram { name = "concierge-control"; runtime = herdrPackage; };
          concierge-supervise = mkProgram { name = "concierge-supervise"; runtime = herdrPackage; };
          tincan = pkgs.symlinkJoin {
            name = "tincan-0.1.0";
            paths = [ tincan-command tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive ];
          };
          concierge = pkgs.symlinkJoin {
            name = "concierge-0.1.0";
            paths = [ concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise ]
              ++ pkgs.lib.optionals pkgs.stdenv.isLinux [ concierge-primary ];
          };
        in
        {
          default = tincan;
          inherit tincan concierge tincan-command tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive concierge-brief concierge-worker concierge-backlog concierge-task concierge-migrate concierge-control concierge-supervise;
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux { inherit concierge-primary; });

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
          inherit (packages) tincan-command tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive concierge-brief concierge-worker concierge-backlog concierge-task concierge-migrate concierge-control concierge-supervise;
          tincan-smoke = pkgs.runCommand "tincan-smoke" { } ''
            unset TINCAN_HOME HERDR_SESSION HERDR_SOCKET_PATH HERDR_WORKSPACE_ID HERDR_TAB_ID HERDR_PANE_ID
            export PATH=
            test -x ${packages.tincan-command}/bin/tincan
            test "$(${packages.tincan}/bin/tincan --version)" = 0.1.0
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox send smoke --body 'Packaged message' > sent
            ${pkgs.gnugrep}/bin/grep -q '^id: ' sent
            ${packages.tincan}/bin/tincan --root "$TMPDIR/messages" inbox list smoke > pending
            ${pkgs.gnugrep}/bin/grep -q '^count: 1$' pending
            ${packages.tincan}/bin/tincan notify --help >/dev/null
            : > "$out"
          '';
          go-source = pkgs.runCommand "tincan-go-source" { src = (goModule pkgs).src; } ''
            test -e "$src/go.mod"
            test -e "$src/cmd"
            test -e "$src/pkg"
            test ! -e "$src/README.md"
            touch "$out"
          '';
          concierge-smoke = pkgs.runCommand "concierge-smoke" { } ''
            ${packages.concierge-worker}/bin/concierge-worker --help >/dev/null
            ${packages.concierge-backlog}/bin/concierge-backlog --help >/dev/null
            mkdir "$TMPDIR/concierge"
            PATH=/usr/bin:/bin ${packages.concierge-backlog}/bin/concierge-backlog \
              --root "$TMPDIR/concierge" add smoke-a1 'Packaged intake' \
              --kind task --repo exmachina >/dev/null
            grep -q 'smoke-a1 - Packaged intake' "$TMPDIR/concierge/backlog.md"
            PATH=/usr/bin:/bin ${packages.concierge-backlog}/bin/concierge-backlog \
              --root "$TMPDIR/concierge" list \
              | grep 'smoke-a1,queued,task,exmachina,Packaged intake' >/dev/null
            PATH=/usr/bin:/bin ${packages.concierge-brief}/bin/concierge-brief \
              --root "$TMPDIR/concierge" --message-root "$TMPDIR/messages" \
              | grep 'smoke-a1,queued,task,exmachina,Packaged intake' >/dev/null
            ${packages.concierge-task}/bin/concierge-task --help >/dev/null
            ${packages.concierge-migrate}/bin/concierge-migrate --help >/dev/null
            ${pkgs.lib.optionalString pkgs.stdenv.isLinux ''${packages.concierge-primary}/bin/concierge-primary --help >/dev/null''}
            for command in concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise; do
              test -x "${packages.concierge}/bin/$command"
            done
            test ! -e ${packages.concierge}/bin/concierge-migrate
            ${pkgs.lib.optionalString pkgs.stdenv.isLinux ''test -x "${packages.concierge}/bin/concierge-primary"''}
            for command in tincan-inbox tincan-notify tincan-task-send tincan-task-reply tincan-task-receive; do
              test -x "${packages.tincan}/bin/$command"
            done
            test ! -e ${packages.tincan}/bin/concierge-brief
            test ! -e ${packages.concierge}/bin/tincan-inbox
            ${packages.tincan-task-send}/bin/tincan-task-send --help >/dev/null
            ${packages.tincan-task-reply}/bin/tincan-task-reply --help >/dev/null
            ${packages.tincan-task-receive}/bin/tincan-task-receive --help >/dev/null
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
          go-tests = buildCommands pkgs;
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux { inherit (packages) concierge-primary; });

      devShells = forEachSystem (pkgs:
        let
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
        in
        {
          default = pkgs.mkShell {
            packages = [ pkgs.go_1_25 pkgs.gopls pkgs.golines tasksAxi herdr.packages.${pkgs.stdenv.hostPlatform.system}.default ];
          };
        });
    };
}
