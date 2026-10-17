# KubeVirt containerDisk: a container image whose only content is the VM disk at
# /disk/<file>. virt-launcher reads it as the VM's (read-only, copy-on-write) root disk.
# 107 is the qemu user inside virt-launcher, which must be able to read the file.
FROM scratch
ADD --chown=107:107 disk.qcow2 /disk/disk.qcow2
