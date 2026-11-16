log "Reiniciando $(hostname) em 3s"
systemd-run --quiet --on-active=3 --timer-property=AccuracySec=100ms /bin/systemctl reboot
