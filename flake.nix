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
      goModule = pkgs: {
        version = "0.1.0";
        src = self;
        go = pkgs.go_1_25;
        vendorHash = "sha256-tVSpcHc3/3/iTdcl0dpOU/pmiR/ljhrKDIw3Op7qK+U=";
      };
    in
    {
      packages = forEachSystem (pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          herdrPackage = herdr.packages.${system}.default;
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
          mkProgram = { name, runtime ? null }: pkgs.buildGoModule ((goModule pkgs) // {
            pname = name;
            subPackages = [ "cmd/${name}" ];
            doCheck = false;
            nativeBuildInputs = pkgs.lib.optionals (runtime != null) [ pkgs.makeWrapper ];
            disallowedRequisites = pkgs.lib.optionals (runtime == null) [ herdrPackage ];
            postFixup = pkgs.lib.optionalString (runtime != null) ''
              wrapProgram "$out/bin/${name}" --prefix PATH : ${runtime}/bin
            '';
          });
          tincan-inbox = mkProgram { name = "tincan-inbox"; };
          tincan-notify = mkProgram { name = "tincan-notify"; runtime = herdrPackage; };
          tincan-task-send = mkProgram { name = "tincan-task-send"; runtime = herdrPackage; };
          tincan-firstmate-send = mkProgram { name = "tincan-firstmate-send"; runtime = herdrPackage; };
          tincan-firstmate-check = mkProgram { name = "tincan-firstmate-check"; runtime = herdrPackage; };
          tincan-firstmate-watch = mkProgram { name = "tincan-firstmate-watch"; runtime = herdrPackage; };
          tincan-firstmate-show = mkProgram { name = "tincan-firstmate-show"; };
          concierge-brief = mkProgram { name = "concierge-brief"; runtime = tasksAxi; };
          concierge-primary = mkProgram { name = "concierge-primary"; };
          concierge-worker = mkProgram { name = "concierge-worker"; };
          concierge-backlog = mkProgram { name = "concierge-backlog"; runtime = tasksAxi; };
          concierge-task = mkProgram { name = "concierge-task"; runtime = herdrPackage; };
          concierge-control = mkProgram { name = "concierge-control"; runtime = herdrPackage; };
          concierge-supervise = mkProgram { name = "concierge-supervise"; runtime = herdrPackage; };
          tincan = pkgs.symlinkJoin {
            name = "tincan-0.1.0";
            paths = [ tincan-inbox tincan-notify tincan-task-send tincan-firstmate-send tincan-firstmate-check tincan-firstmate-watch tincan-firstmate-show ];
          };
          concierge = pkgs.symlinkJoin {
            name = "concierge-0.1.0";
            paths = [ concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise ]
              ++ pkgs.lib.optionals pkgs.stdenv.isLinux [ concierge-primary ];
          };
        in
        {
          default = tincan;
          inherit tincan concierge tincan-inbox tincan-notify tincan-task-send tincan-firstmate-send tincan-firstmate-check tincan-firstmate-watch tincan-firstmate-show concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise;
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
          tasksAxi = pkgs.callPackage ./nix/tasks-axi.nix { };
        in
        {
          inherit (packages) tincan-inbox tincan-notify tincan-task-send tincan-firstmate-send tincan-firstmate-check tincan-firstmate-watch tincan-firstmate-show concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise;
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
              | grep -q 'smoke-a1,queued,task,exmachina,Packaged intake'
            PATH=/usr/bin:/bin ${packages.concierge-brief}/bin/concierge-brief \
              --root "$TMPDIR/concierge" --message-root "$TMPDIR/messages" \
              | grep -q 'smoke-a1,queued,task,exmachina,Packaged intake'
            ${packages.concierge-task}/bin/concierge-task --help >/dev/null
            ${pkgs.lib.optionalString pkgs.stdenv.isLinux ''${packages.concierge-primary}/bin/concierge-primary --help >/dev/null''}
            for command in concierge-brief concierge-worker concierge-backlog concierge-task concierge-control concierge-supervise; do
              test -x "${packages.concierge}/bin/$command"
            done
            ${pkgs.lib.optionalString pkgs.stdenv.isLinux ''test -x "${packages.concierge}/bin/concierge-primary"''}
            for command in tincan-inbox tincan-notify tincan-task-send tincan-firstmate-send tincan-firstmate-check tincan-firstmate-watch tincan-firstmate-show; do
              test -x "${packages.tincan}/bin/$command"
            done
            test ! -e ${packages.tincan}/bin/concierge-brief
            test ! -e ${packages.concierge}/bin/tincan-inbox
            ${packages.tincan-firstmate-send}/bin/tincan-firstmate-send --help >/dev/null
            ${packages.tincan-task-send}/bin/tincan-task-send --help >/dev/null
            ${packages.tincan-firstmate-check}/bin/tincan-firstmate-check --help >/dev/null
            ${packages.tincan-firstmate-watch}/bin/tincan-firstmate-watch --help >/dev/null
            ${packages.tincan-firstmate-show}/bin/tincan-firstmate-show --help >/dev/null
            touch "$out"
          '';
          go-format = pkgs.runCommand "tincan-go-format" {
            nativeBuildInputs = [ pkgs.golines pkgs.go_1_25 ];
            src = self;
          } ''
            cd "$src"
            unformatted="$(golines ${golinesFlags} -l .)"
            if [ -n "$unformatted" ]; then
              printf 'Go files need formatting:\n%s\n' "$unformatted" >&2
              exit 1
            fi
            touch "$out"
          '';
          go-tests = pkgs.buildGoModule ((goModule pkgs) // {
            pname = "tincan-go-tests";
            subPackages = [ "cmd/..." ];
            checkPhase = ''
              runHook preCheck
              export PATH=${tasksAxi}/bin:$PATH
              go test ./...
              runHook postCheck
            '';
          });
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
