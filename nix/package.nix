{ lib, buildGoModule, src, version, vendorHash, webManifest, webArchive, allowDirty ? false }:
buildGoModule {
  pname = "zennotes-server";
  inherit src version vendorHash;
  subPackages = [ "cmd/zennotes-server" ];
  tags = [ "embed_web" ];
  ldflags = [ "-s" "-w" ];
  postConfigure = ''
    go run ./cmd/prepare-web -manifest ${webManifest} -archive ${webArchive} -output web/dist ${lib.optionalString allowDirty "-allow-dirty"}
    go test -tags=embed_web ./web
  '';
  meta = {
    description = "A server API for hosting remote ZenNotes vaults";
    homepage = "https://zennotes.org/";
    license = lib.licenses.mit;
    mainProgram = "zennotes-server";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}
