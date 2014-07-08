## Snakk

Chat-server with an IRC-like web frontend.

### Install (Ubuntu/Debian)

```bash
wget https://github.com/boutros/snakk/releases/download/v0.5/snakk_v0.5.deb
sudo dpkg -i snakk_v0.5.deb
sudo service snakk start
```

This will set it up as an upstart service. The application will be installed to `/usr/share/snakk`. You'll find a config file there. You must restart the service for any configuration changes to take effect.

To uninstall, run `sudo dpkg -r snakk`
