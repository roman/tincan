{
  description = "Independent agent communication and supervision tools";

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
          tincan-control = mkProgram { name = "tincan-control"; runtime = herdrPackage; };
          tincan-supervise = mkProgram { name = "tincan-supervise"; runtime = herdrPackage; };
          tincan-firstmate-send = mkProgram { name = "tincan-firstmate-send"; runtime = herdrPackage; };
          tincan-firstmate-check = mkProgram { name = "tincan-firstmate-check"; runtime = herdrPackage; };
          tincan = pkgs.symlinkJoin {
            name = "tincan-0.1.0";
            paths = [ tincan-inbox tincan-notify tincan-control tincan-supervise tincan-firstmate-send tincan-firstmate-check ];
          };
        in
        {
          default = tincan;
          inherit tincan tincan-inbox tincan-notify tincan-control tincan-supervise tincan-firstmate-send tincan-firstmate-check;
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
          inherit (packages) tincan-inbox tincan-notify tincan-control tincan-supervise tincan-firstmate-send tincan-firstmate-check;
          firstmate-smoke = pkgs.runCommand "tincan-firstmate-smoke" { } ''
            ${packages.tincan-firstmate-send}/bin/tincan-firstmate-send --help >/dev/null
            ${packages.tincan-firstmate-check}/bin/tincan-firstmate-check --help >/dev/null
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
              go test ./...
              runHook postCheck
            '';
          });
        });

      devShells = forEachSystem (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go_1_25 pkgs.gopls pkgs.golines herdr.packages.${pkgs.stdenv.hostPlatform.system}.default ];
        };
      });
    };
}
