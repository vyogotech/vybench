# Droplet storage

The DigitalOcean image keeps vybench data at `/var/snap/vybench/common`. That directory holds MariaDB, sites, and uploads. Strict confinement only allows the snap to write there, so a volume is bind-mounted onto that path instead of moving the path.

## Attach a volume when you create the Droplet

In Additional Storage, add a Block Storage volume. 25 GB is a practical start for one ERPNext site. The image does not create a volume for you. A Marketplace image cannot attach storage on its own.

On first boot, `vybench-volume.service` runs `/opt/vybench/mount_volume.sh` before MariaDB starts:

- No volume attached: the script exits and data stays on the Droplet disk.
- A volume DigitalOcean has already mounted under `/mnt/volume_*`: the script copies `/var/snap/vybench/common` onto it and bind-mounts that directory back over the snap data path.
- A volume that is attached but not mounted: the script formats it ext4 once, mounts it, and does the same bind.

The login banner and `/root/.vybench_credentials` say which of the two you are on. `findmnt /var/snap/vybench/common` shows a bind mount when the volume is in use.

A volume attached after the first boot is picked up on the next reboot, before the site is created if first boot has not finished, and before MariaDB on later boots.

## Backups

1. `bench backup` from the Droplet, while the site is up.
2. A DigitalOcean volume snapshot, taken while the Droplet is off or the volume is quiescent.
3. A copy of the backup files to Spaces or another machine.

The volume can be detached and attached to a replacement Droplet. The replacement must be created from this image so the same bind runs at boot.
