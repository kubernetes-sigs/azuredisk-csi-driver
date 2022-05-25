go install
mkdir -p /var/tmp/azuredisk-csi-driver/cmd/az-log/config
rm -f /var/tmp/azuredisk-csi-driver/cmd/az-log/config/az-log.yaml
ln -s $PWD/config/az-log.yaml /var/tmp/azuredisk-csi-driver/cmd/az-log/config/az-log.yaml
echo "Installation is completed"
