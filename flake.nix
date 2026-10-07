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
          tincan = pkgs.symlinkJoin {
            name = "tincan-0.1.0";
            paths = [ tincan-inbox tincan-notify tincan-control tincan-supervise ];
          };
        in
        {
          default = tincan;
          inherit tincan tincan-inbox tincan-notify tincan-control tincan-supervise;
        });

      checks = forEachSystem (pkgs:
        let
          packages = self.packages.${pkgs.stdenv.hostPlatform.system};
        in
        {
          inherit (packages) tincan-inbox tincan-notify tincan-control tincan-supervise;
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
          packages = [ pkgs.go_1_25 pkgs.gopls herdr.packages.${pkgs.stdenv.hostPlatform.system}.default ];
        };
      });
    };
}
