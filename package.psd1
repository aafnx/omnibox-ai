@{
    # Application files copied into the installation directory. Build, installer and tests read this list.
    Files = @(
        'VERSION', 'ai.ps1', 'ai.cmd', 'panel.ps1', 'service.ps1',
        'src/Service.Host.cs', 'src/Service.Install.psm1', 'scripts/build-service.ps1', 'src/OmniBox.Ai.psm1',
        'src/Panel.Engine.ps1', 'src/Panel.Cli.ps1', 'src/Telemetry.ps1', 'src/Inference.Stats.cs',
        'src/Gateway.Helpers.psm1', 'src/OpenAi.Proxy.cs', 'src/Model.Catalog.psm1', 'src/Panel.Server.psm1',
        'src/Panel.Security.psm1', 'src/Panel.Install.psm1', 'src/Install.Helpers.psm1',
        'src/Update.Helpers.psm1', 'src/Web.Update.psm1', 'scripts/update-worker.ps1', 'scripts/get-install.ps1',
        'src/Setup.Helpers.psm1',
        'web/index.html', 'web/style.css', 'web/app.js', 'web/favicon.svg', 'web/brand-light.svg', 'web/brand-dark.svg',
        'web/inter-400.woff.base64', 'web/inter-600.woff.base64', 'web/inter-license.txt',
        'config.example.json', 'README.md',
        'docs/commands.md', 'docs/architecture.md', 'docs/install.md', 'docs/web-panel.md', 'docs/reverse-proxy.md',
        'docs/model-api.md', 'docs/models.md', 'docs/monitoring.md'
    )
    # Installer-only files: present in the package, never copied into the installation directory.
    InstallerFiles = @('install.ps1', 'package.psd1')
    # Packaged text uses fixed line endings, independent of the checkout (core.autocrlf, .gitattributes).
    CrlfExtensions = @('.ps1', '.psm1', '.psd1', '.cmd')
}
