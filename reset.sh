bbctl delete sh-tiktok
rm sh-tiktok.db
rm config.yaml
sleep 2
bbctl config --type bridgev2 -o config.yaml sh-tiktok
