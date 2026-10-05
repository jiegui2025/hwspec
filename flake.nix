{
  description = "hwspec: capture a Linux machine's hardware specification to JSON/YAML";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        hwspec = pkgs.buildGoModule {
          pname = "hwspec";
          version = self.shortRev or "dirty";
          src = ./.;
          vendorHash = "sha256-jEewyc8zKTRsE3NgNlHovuEeSiW9QeiaeL7Z7pTDZVU=";
          subPackages = [ "cmd/hwspec" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=${self.shortRev or "dirty"}" ];
          meta = {
            description = "Capture a Linux machine's hardware specification to JSON/YAML";
            mainProgram = "hwspec";
            platforms = pkgs.lib.platforms.linux;
          };
        };
        default = hwspec;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls ]; };
      });
    };
}
