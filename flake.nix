{
  description = "Tincan durable agent messages and notifications";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/release-25.11";
    herdr.url = "github:herdrdev/herdr/v0.9.3";
    flake-parts.url = "github:hercules-ci/flake-parts";
    nixDir = {
      url = "github:roman/nixDir/v3";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
    };
  };

  outputs = inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [ inputs.nixDir.flakeModule ./nix/checks.nix ];
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      nixDir = {
        enable = true;
        root = ./.;
        importWithInputs = true;
      };
      perSystem = { self', ... }: {
        packages.default = self'.packages.tincan;
      };
    };
}
